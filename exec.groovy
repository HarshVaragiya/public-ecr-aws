pipeline {
    agent any
    stages {
        stage("build"){
            steps {
                echo 'building'
                sh 'ls -alh'
            }
        }
        stage("exec") {
            steps{
                echo 'executing '
            }
        }
        stage("cleanup"){
            steps {
                echo 'cleaning up'
            }
        }
    }
}