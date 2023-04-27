pipeline {
    agent any
    stages {
        stage("login"){
            steps {
                sh "aws ecr get-login-password --region ap-south-2 | docker login --username AWS --password-stdin ${REPOSITORY}"
            }
        }
        stage("pull") {
            steps{
                sh "docker pull ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG}"
                sh "docker tag ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG} public-ecr-aws:latest"
            }
        }
        stage("exec") {
            steps{
                withCredentials([string(credentialsId: 'gotify-url', variable: 'GOTIFY_URL')]) {
                    sh '''
                    curl ${GOTIFY_URL} \
                        -F "title=${JOB_NAME} trigerrered" \
                        -F "message=build #${BUILD_NUMBER} in progress ${BUILD_URL}. Program Args: ${ARGS}" \
                        -F "priority=9"
                    '''
                }
                withCredentials([string(credentialsId: 'gotify-url', variable: 'GOTIFY_URL')]) {
                    withCredentials([usernamePassword(credentialsId: 'redis-connection-string', passwordVariable: 'REDIS_PASSWORD', usernameVariable: 'REDIS_HOST')]) {
                        sh '''docker run --rm \
                        -e REDIS_HOST=${REDIS_HOST} \
                        -e REDIS_PASSWORD=${REDIS_PASSWORD} \
                        -e GOTIFY_URL=${GOTIFY_URL} \
                        -v $PWD:/pwd \
                        public-ecr-aws:latest ${ARGS}
                        '''
                    }
                }
            }
        }
        stage("cleanup"){
            steps {
                build('docker-cleanup')
            }
        }
    }
}