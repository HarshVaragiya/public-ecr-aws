pipeline {
    agent any
    stages {
        stage("build"){
            steps {
                echo 'building'
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